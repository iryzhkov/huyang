#include "api.hpp"
#include <cassert>
int main() { assert(billing::quote_total(3) == 21); assert(billing::quote_total(-1) == 0); assert(billing::quote_total(std::string("x")) == "quote_total:x"); }
