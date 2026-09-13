/** The amount left in the ledger. */
export function total(): number {
  return 7;
}

/** Nothing calls this, so a reference-checked deletion may remove it. */
export function unused(): number {
  return 0;
}
