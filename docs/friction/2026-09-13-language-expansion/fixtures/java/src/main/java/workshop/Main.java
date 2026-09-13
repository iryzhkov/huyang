package workshop;
public final class Main {
 public static void main(String[] args) {
  if (Billing.quoteTotal(3) != 21 || Billing.quoteTotal(-1) != 0 || !Billing.quoteTotal("x").equals("quoteTotal:x")) throw new AssertionError("billing");
 }
}
