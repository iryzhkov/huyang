export function leaf(n: number): number { return n * 2; }
export function indirect(callback: (n: number) => number, n: number): number {
  return callback(n);
}
export async function run(n: number): Promise<number> {
  if (n < 0) throw new Error("negative input");
  const value = await Promise.resolve(n).then(x => indirect(leaf, x));
  return value;
}
export { leaf as barrelLeaf } from "./leaf";
