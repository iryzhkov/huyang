package bridge

type schedulerClass string

const (
	schedulePureRead       schedulerClass = "pure_read"
	scheduleProviderRead   schedulerClass = "provider_read"
	scheduleCanonicalWrite schedulerClass = "canonical_write"
	scheduleSandboxWrite   schedulerClass = "sandbox_write"
	scheduleExternalJob    schedulerClass = "external_job"
)

func knownSchedulerClass(class schedulerClass) bool {
	switch class {
	case schedulePureRead, scheduleProviderRead, scheduleCanonicalWrite, scheduleSandboxWrite, scheduleExternalJob:
		return true
	default:
		return false
	}
}

// modernSchedulerClass returns the class a tool declares on its descriptor.
// Unknown tools fall back to the most exclusive class so a registration
// mistake degrades to serialisation rather than to an unguarded provider call.
func modernSchedulerClass(name string) schedulerClass {
	for _, descriptor := range modernTools {
		if descriptor.Name == name {
			return descriptor.Class
		}
	}
	return scheduleCanonicalWrite
}

// classForCall refines the declared class for argument-dependent behaviour:
// change_plan actions other than prepare only touch durable plan intent and
// the canonical tree, and a read addressed by symbol locator may consult the
// provider to resolve the declaration.
func classForCall(name string, arguments map[string]any) schedulerClass {
	class := modernSchedulerClass(name)
	switch name {
	case "change_plan":
		if action, _ := arguments["action"].(string); action != "prepare" {
			class = scheduleCanonicalWrite
		}
	case "read":
		if target, _ := arguments["target"].(map[string]any); target != nil {
			if _, symbol := target["symbol_locator"]; symbol {
				class = scheduleProviderRead
			}
		}
	}
	return class
}
