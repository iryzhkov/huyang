#include "api.h"
#include <assert.h>
#include <string.h>
int main(void) { const char *label = "quote_total"; assert(quote_total(3) == 21); assert(quote_total(-1) == 0); assert(strcmp(label, "quote_total") == 0); return 0; }
