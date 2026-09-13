import { formatLabel } from "./index.ts";
export const diagnosticName = "formatLabel";
export function greet(name: string): string {
  return `Hello ${formatLabel(name)}`;
}
