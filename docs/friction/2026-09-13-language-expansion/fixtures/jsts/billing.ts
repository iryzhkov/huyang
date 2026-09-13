export function quoteTotal(units: number): number { return units < 0 ? 0 : units * 7; }
export const diagnosticName = "quoteTotal";
