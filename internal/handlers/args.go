package handlers

import (
	"errors"
	"fmt"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// argInt reads an integer tool argument. JSON numbers decode as float64, so
// the value is narrowed here; a missing or non-numeric argument yields def.
func argInt(args map[string]any, key string, def int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return def
}

func uintArgument(value any) uint64 {
	number, _ := value.(float64)
	return uint64(number)
}

func decodeRangeHandle(value map[string]any) (workspacecore.RangeHandle, error) {
	if value == nil {
		return workspacecore.RangeHandle{}, errors.New("target.file_range is required")
	}
	integer := func(key string) int {
		number, _ := value[key].(float64)
		return int(number)
	}
	handle := workspacecore.RangeHandle{
		Path: fmt.Sprint(value["path"]), Revision: workspacecore.RevisionID(fmt.Sprint(value["revision_id"])),
		ByteStart: integer("byte_start"), ByteEnd: integer("byte_end"),
		ExpectedSHA256: fmt.Sprint(value["expected_sha256"]), BeforeSHA256: fmt.Sprint(value["before_sha256"]),
		AfterSHA256: fmt.Sprint(value["after_sha256"]), AnchorBytes: integer("anchor_bytes"),
	}
	if handle.Path == "" || handle.Revision == "" {
		return workspacecore.RangeHandle{}, errors.New("file range path and revision_id are required")
	}
	return handle, nil
}
