package workshop;
public final class Billing {
 public static int quoteTotal(int units) { return units < 0 ? 0 : units * 7; }
 public static String quoteTotal(String label) { return "quoteTotal:" + label; }
}
