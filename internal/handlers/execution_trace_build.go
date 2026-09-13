package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const maxTraceExecutableBytes = 256 << 20

func traceLaunchIdentity(ctx context.Context, w *workspacecore.Workspace, args map[string]any) map[string]string {
	launch := map[string]string{"request": fmt.Sprint(args["action"]), "adapter": fmt.Sprint(args["adapter"]), "arguments": "omitted", "environment": "omitted",
		"build_identity": "adapter_managed_build_not_fingerprinted", "source_identity": "captured_before_launch"}
	if file, _ := args["file"].(string); file != "" {
		launch["file"] = filepath.Base(file)
	}
	program, _ := args["program"].(string)
	if program == "" {
		return launch
	}
	if !filepath.IsAbs(program) {
		program = filepath.Join(w.Identity().Root, program)
	}
	info, err := os.Stat(program)
	if err != nil || !info.Mode().IsRegular() || filepath.Ext(program) == ".go" {
		return launch
	}
	if info.Size() > maxTraceExecutableBytes {
		launch["build_identity"] = "executable_exceeds_fingerprint_bound"
		return launch
	}
	file, err := os.Open(program)
	if err != nil {
		return launch
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	remaining := int64(maxTraceExecutableBytes) + 1
	for remaining > 0 {
		if ctx.Err() != nil {
			return launch
		}
		n, err := file.Read(buffer)
		if n > 0 {
			remaining -= int64(n)
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return launch
		}
	}
	if remaining <= 0 {
		return launch
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		launch["build_identity"] = "executable_changed_during_capture"
		return launch
	}
	launch["build_identity"] = "explicit_executable_sha256"
	launch["executable_sha256"] = fmt.Sprintf("%x", hash.Sum(nil))
	launch["source_identity"] = "launch_snapshot_not_verified_against_binary"
	return launch
}
