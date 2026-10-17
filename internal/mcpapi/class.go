package mcpapi

type SchedulerClass string

const (
	ClassPureRead       SchedulerClass = "pure_read"
	ClassProviderRead   SchedulerClass = "provider_read"
	ClassCanonicalWrite SchedulerClass = "canonical_write"
	ClassSandboxWrite   SchedulerClass = "sandbox_write"
	ClassExternalJob    SchedulerClass = "external_job"
)

func KnownClass(class SchedulerClass) bool {
	switch class {
	case ClassPureRead, ClassProviderRead, ClassCanonicalWrite, ClassSandboxWrite, ClassExternalJob:
		return true
	default:
		return false
	}
}

// ToolClass returns the class a tool declares on its descriptor.
// Unknown tools fall back to the most exclusive class so a registration
// mistake degrades to serialisation rather than to an unguarded provider call.
func ToolClass(name string) SchedulerClass {
	for _, descriptor := range Tools {
		if descriptor.Name == name {
			return descriptor.Class
		}
	}
	return ClassCanonicalWrite
}

// ClassForCall refines the declared class for argument-dependent behaviour:
// change_plan actions other than prepare only touch durable plan intent and
// the canonical tree, and a read addressed by symbol locator may consult the
// provider to resolve the declaration.
func ClassForCall(name string, arguments map[string]any) SchedulerClass {
	class := ToolClass(name)
	switch name {
	case "change_plan":
		if action, _ := arguments["action"].(string); action != "prepare" {
			class = ClassCanonicalWrite
		}
	case "read":
		if target, _ := arguments["target"].(map[string]any); target != nil {
			if _, symbol := target["symbol_locator"]; symbol {
				class = ClassProviderRead
			}
		}
	}
	return class
}
