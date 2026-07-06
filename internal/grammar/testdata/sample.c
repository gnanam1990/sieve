#include <stdio.h>

int add(int a, int b) {
    return a + b;
}

typedef struct {
    int x;
    int y;
} Point;

// create returns a new point.
Point create(int x, int y) {
    Point p;
    p.x = x;
    p.y = y;
    return p;
}
