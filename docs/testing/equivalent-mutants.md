# Equivalent mutants

Surviving gremlins mutants that cannot change observable behaviour, so no test can kill them.
Format: `path:line TYPE — why`.

## internal/database

- `db.go:1061` CONDITIONALS_BOUNDARY — `len(remark) > 256` vs `>= 256`: cutting a 256-byte remark to 256 bytes leaves it unchanged.
- `model/model.go:813` CONDITIONALS_BOUNDARY — `len(s) <= 32` vs `< 32`: at exactly 32 the tail `s[32:]` is empty and the function returns "" either way.

## internal/util/link

- `outbound.go:1350` CONDITIONALS_BOUNDARY — `i < len(keys)` vs `<=`: at `i == len(keys)` the inner loop starts at `len+1` and does nothing.
- `outbound.go:1352` CONDITIONALS_BOUNDARY — `keys[j] < keys[i]` vs `<=`: the keys come from a map and are never equal.
- `outbound.go:466, 471` CONDITIONALS_BOUNDARY — `i >= 0` vs `> 0` for `#` and `?`: the link starts with `ss://`, so neither can be at index 0.

- `outbound.go:1470` CONDITIONALS_BOUNDARY — `len(r) > maxSlugRunes` vs `>=`: the slug is already trimmed of dashes at both ends, so cutting a slug of exactly `maxSlugRunes` runes leaves it unchanged.

Not pinned on purpose: `outbound.go:494, 516` (`colon < 0` vs `<= 0` for `ss://…@:443`). Today a shadowsocks link with an empty host parses; a test would freeze that questionable behaviour instead of a wanted one.

## internal/web/runtime

- `remote.go:564` CONDITIONALS_BOUNDARY — `len(env.Obj) > 0` vs `>= 0`: an empty obj fails `json.Unmarshal` right after, so nothing is cached either way.
