import assert from "node:assert/strict";
import { quoteTotal, diagnosticName } from "./index.ts";
assert.equal(quoteTotal(3),21); assert.equal(quoteTotal(-1),0); assert.equal(diagnosticName,"quoteTotal");
