# Equivalent mutants

Surviving gremlins mutants that cannot change observable behaviour, so no test can kill them.
Format: `path:line TYPE — why`.

## internal/database

- `db.go:1119, 1152, 1169, 1227, 1303, 1349, 1374, 1388, 1408, 1451, 1513` CONDITIONALS_BOUNDARY / CONDITIONALS_NEGATION — the guarded block only writes a log line with the count (`if n > 0 { log.Printf(...) }`).
- `db.go:1225, 1347, 1511` INCREMENT_DECREMENT, `db.go:1408` ARITHMETIC_BASE — the counter is read only by that log line.
- `db.go:1061` CONDITIONALS_BOUNDARY — `len(remark) > 256` vs `>= 256`: cutting a 256-byte remark to 256 bytes leaves it unchanged.
- `model/model.go:813` CONDITIONALS_BOUNDARY — `len(s) <= 32` vs `< 32`: at exactly 32 the tail `s[32:]` is empty and the function returns "" either way.

## internal/util/link

- `outbound.go:1350` CONDITIONALS_BOUNDARY — `i < len(keys)` vs `<=`: at `i == len(keys)` the inner loop starts at `len+1` and does nothing.
- `outbound.go:1352` CONDITIONALS_BOUNDARY — `keys[j] < keys[i]` vs `<=`: the keys come from a map and are never equal.
- `outbound.go:466, 471` CONDITIONALS_BOUNDARY — `i >= 0` vs `> 0` for `#` and `?`: the link starts with `ss://`, so neither can be at index 0.
