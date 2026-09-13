#include "api.hpp"
namespace billing { int quote_total(int units) { return units < 0 ? 0 : units * 7; } std::string quote_total(const std::string &label) { return "quote_total:" + label; } }
