package embed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDebugActivityDoesNotStartAReplacement(t *testing.T) {
	b := &Backend{
		config:     Config{Root: "unused", Debug: true, Executable: "huyang-test-missing-neovim"},
		generation: &generation{epoch: 1, done: make(chan error)},
	}
	if active, err := b.DebugActive(context.Background()); err != nil || active {
		t.Fatalf("dead generation retained debug ownership: %v %v", active, err)
	}
	b.generation = nil
	if active, err := b.DebugActive(context.Background()); err != nil || active {
		t.Fatalf("absent generation retained debug ownership: %v %v", active, err)
	}
}

func TestReplacementWaitsForRetiringProcessTree(t *testing.T) {
	b := &Backend{
		config:     Config{Root: "unused", Executable: "huyang-test-missing-neovim"},
		generation: &generation{epoch: 1, done: make(chan error)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.ensureGeneration(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("attempted replacement before the old tree exited: %v", err)
	}
}

func TestCloseWaitsForAlreadyDeadGenerationTree(t *testing.T) {
	g := &generation{done: make(chan error)}
	b := &Backend{generation: g, done: make(chan struct{})}
	closed := make(chan struct{})
	go func() { _ = b.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
		t.Fatal("reservation released before retiring tree exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(g.done)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after tree exit")
	}
}

// Exercise the installed wrapper against Neovim's real Client implementation,
// including asynchronous transport exit and init callbacks, with no server install.
func TestOwnedLanguageServerCapacityAndDebugAdoption(t *testing.T) {
	backend, _, _ := testBackend(t, false, func(c *Config) {
		c.MaxLanguageServers = 2
		c.Debug = true // Debug mode alone must not permanently pin clients.
		c.InitFile = filepath.Join(t.TempDir(), "init.lua")
		if err := os.WriteFile(c.InitFile, []byte("vim.g.limits_before_init = package.loaded['huyang.limits'] ~= nil\n"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	execLua(t, backend, `
  assert(vim.g.limits_before_init, "limits missing before user init")
  _G.limit_exited = {}
  _G.limit_start = function(name)
   return vim.lsp.start({
    name=name, root_dir=vim.fn.getcwd(),
    cmd=function(dispatchers)
     local stopped=false
     return {
      request=function(method,_,callback)
       if method == "initialize" then
        vim.schedule(function() callback(nil,{capabilities={}}) end)
       elseif method == "shutdown" then
        vim.schedule(function() callback(nil,nil) end)
       end
       return true,1
      end,
      notify=function() return true end,
      is_closing=function() return stopped end,
      terminate=function()
       stopped=true
       vim.defer_fn(function() dispatchers.on_exit(0,0) end,20)
      end,
     }
    end,
    on_exit=function() _G.limit_exited[name]=true end,
   },{attach=false})
  end
  _G.limit_a = limit_start("limit_a")
  _G.limit_b = limit_start("limit_b")
  assert(vim.wait(1000,function()
   return vim.lsp.get_client_by_id(limit_a).initialized and vim.lsp.get_client_by_id(limit_b).initialized
  end))
  assert(limit_start("limit_a") == limit_a)
  local c = assert(limit_start("limit_c"),"LRU admission failed")
  assert(vim.wait(1000,function() return vim.lsp.get_client_by_id(c).initialized end))
  assert(limit_exited.limit_b and not limit_exited.limit_a,"wrong victim")
  assert(#vim.lsp.get_clients() == 2,"client count = " .. #vim.lsp.get_clients())
 `)
	execLua(t, backend, `
  local dap = require("huyang.dap")
  assert(not dap.active(), "discovery claimed debug ownership")
  dap._state().session = { closed = false }
 `)
	if active, err := backend.DebugActive(context.Background()); err != nil || !active {
		t.Fatalf("active session not observed: %v %v", active, err)
	}
	execLua(t, backend, `
  local id=limit_start("limit_d")
  assert(id==nil,"active debug session did not pin language servers")
  assert(#vim.lsp.get_clients()==2 and not limit_exited.limit_a and not limit_exited.limit_c)
  require("huyang.dap")._state().exit = { reason = "exited" }
 `)
	if active, err := backend.DebugActive(context.Background()); err != nil || active {
		t.Fatalf("exit retained debug ownership: %v %v", active, err)
	}
	execLua(t, backend, `
  local id=assert(limit_start("limit_after_exit"), "session exit did not unpin clients")
  assert(vim.wait(1000,function() return vim.lsp.get_client_by_id(id).initialized end))
  assert(#vim.lsp.get_clients()==2)
  require("huyang.dap")._state().session = nil
 `)
}
