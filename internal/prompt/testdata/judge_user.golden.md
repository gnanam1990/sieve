
## alpha.txt (modified)

Diff:
@@ -1,6 +1,6 @@ 
R:1   alpha line 1
R:2   alpha line 2
L:3 - alpha line 3
R:3 + alpha line three
R:4   alpha line 4
R:5   alpha line 5
R:6   alpha line 6
@@ -27,14 +27,14 @@ alpha line 26
R:27   alpha line 27
R:28   alpha line 28
R:29   alpha line 29
L:30 - alpha line 30
L:31 - alpha line 31
L:32 - alpha line 32
L:33 - alpha line 33
L:34 - alpha line 34
L:35 - alpha line 35
L:36 - alpha line 36
L:37 - alpha line 37
L:38 - alpha line 38
L:39 - alpha line 39
R:30 + alpha line three0
R:31 + alpha line three1
R:32 + alpha line three2
R:33 + alpha line three3
R:34 + alpha line three4
R:35 + alpha line three5
R:36 + alpha line three6
R:37 + alpha line three7
R:38 + alpha line three8
R:39 + alpha line three9
R:40   alpha line 40

Full file content of alpha.txt:
```
1: alpha line one
2: alpha line two
```

## Findings to verify

[0] Side RIGHT Line 3 severity=major confidence=0.80 category=bug
    Off-by-one on the boundary
    The loop reads past the end.

[1] Side RIGHT Line 5-6 severity=minor confidence=0.50 category=correctness
    Unchecked error
    err is ignored.

