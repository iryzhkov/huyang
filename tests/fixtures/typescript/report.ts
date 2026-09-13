import { total } from "./ledger.js";

/** Reads the total, so an inline and a rename have something to move. */
export function report(): number {
  return total() + 1;
}
