/** The exported interface an impact preview should notice. */
export interface Store {
  balance(): number;
}

/** One implementation. */
export class Memory implements Store {
  constructor(private readonly amount: number) {}
  balance(): number {
    return this.amount;
  }
}

/** Reads the interface rather than either implementation. */
export function sum(store: Store): number {
  return store.balance();
}
