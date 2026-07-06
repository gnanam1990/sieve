#include <stdio.h>

/* add adds two integers. */
int add(int a, int b) {
    return a + b;
}

typedef struct {
    int x;
    int y;
} Point;

Point create(int x, int y) {
    Point p;
    p.x = x;
    p.y = y;
    return p;
}
