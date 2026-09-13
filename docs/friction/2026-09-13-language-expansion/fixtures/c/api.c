#include "api.h"
int quote_total(int units) { return units < 0 ? 0 : units * 7; }
