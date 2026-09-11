package mcpapi

import (
	"encoding/json"
	"testing"
)

func TestModernDebugCatalogIsCompact(t *testing.T) {
	modernDescriptors := make([]ToolDescriptor, 0, 4)
	for _, descriptor := range Catalog(ProfileDebug) {
		if len(descriptor.Name) >= len("debug_") && descriptor.Name[:len("debug_")] == "debug_" {
			modernDescriptors = append(modernDescriptors, descriptor)
		}
	}
	if len(modernDescriptors) != 4 {
		t.Fatalf("modern debugger tools = %d", len(modernDescriptors))
	}
	for _, descriptor := range modernDescriptors {
		encoded, marshalErr := json.Marshal(descriptor)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		t.Logf("%s descriptor bytes = %d", descriptor.Name, len(encoded))
	}
}
