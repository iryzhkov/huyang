import assert from "node:assert/strict";
import { formatLabel } from "./index.ts";
import { greet, diagnosticName } from "./greeting.ts";
assert.equal(formatLabel(" Ada "), "[Ada]");
assert.equal(greet(" Ada "), "Hello [Ada]");
assert.equal(diagnosticName, "formatLabel");
console.log("labels behavior passed");
