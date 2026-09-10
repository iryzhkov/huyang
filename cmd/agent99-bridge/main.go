// Command agent99-bridge exposes the legacy agent loop and MCP stdio server.
package main

import "agent99/internal/bridge"

func main() {
	bridge.Main()
}
