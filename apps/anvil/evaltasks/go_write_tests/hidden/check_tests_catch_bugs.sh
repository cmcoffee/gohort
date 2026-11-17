# The tests must pass, and each change to ParseSize must make one fail.
set -e
ls *_test.go >/dev/null
go test ./... >/dev/null
for m in 's/kb = 1024/kb = 1000/' 's/mb = 1024 \* kb/mb = 1000 * kb/' 's/ || n < 0//' 's/return 0, fmt.Errorf("size %q has no unit", s)/return strconv.ParseInt(s, 10, 64)/'; do
	cp size.go size.go.orig
	sed -i "$m" size.go
	if cmp -s size.go size.go.orig; then echo "change did not apply: $m"; exit 2; fi
	if go test ./... >/dev/null 2>&1; then
		echo "the tests still pass after: $m"
		mv size.go.orig size.go
		exit 1
	fi
	mv size.go.orig size.go
done
