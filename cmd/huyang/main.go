// Command huyang exposes the long-lived Huyang service and its thin MCP adapter.
package main

import "github.com/iryzhkov/huyang/internal/bridge"

func main() {
	bridge.HuyangMain()
}
