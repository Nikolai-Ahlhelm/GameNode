.PHONY:	clean edit editor test test2 gen report report2 assert benchmarks benchmarks2 mem

assert:
	echo "package main; type SourceFileNode struct{}; func (p *parser) sourceFile() *SourceFileNode { return nil }" > parser.go
	go test -v -gen -assert -run TestGen 2>&1 | tee log-gen
	gofmt -l -s -w *.go

clean:
	rm -f cpu.test mem.test *.out
	go clean

edit:
	@touch log
	@if [ -f "Session.vim" ]; then gvim -S & else gvim -p Makefile *.go & fi

editor:
	gofmt -l -s -w *.go
	go test -c -o /dev/null 2>&1 | tee log-editor

gen:
	# echo "package main; type SourceFileNode struct{}; func (p *parser) sourceFile() *SourceFileNode { return nil }" > parser.go
	go test -v -gen -run TestGen 2>&1 | tee log-gen
	gofmt -l -s -w *.go

test:
	go test -v -failfast -heap 2>&1 | tee log-test
	@git diff testdata/ || true
	@git status
	@grep TOTAL log-test || true
	grep 'FAIL' log-test || true

test2:
	go test -v -failfast -src $$HOME/src 2>&1 | tee log-test2
	@git diff testdata/ || true
	@git status
	@grep TOTAL log-test2 || true
	grep 'FAIL' log-test2 || true

report:
	go test -v -failfast -run TestParser -report 2>&1 | tee log-report
	@git diff testdata/ || true
	@git status
	@grep TOTAL log-report || true
	grep 'FAIL' log-report || true

report2:
	go test -v -failfast -run TestParser -src $$HOME/src -report 2>&1 | tee log-report2
	@git diff testdata/ || true
	@git status
	@grep TOTAL log-report2 || true
	grep 'FAIL' log-report2 || true

benchmarks:
	go test -v -run @ -bench . 2>&1 | tee log-benchmarks

benchmarks2:
	go test -v -run @ -bench . -bsrc $$HOME/src 2>&1 | tee log-benchmarks2

mem:
	go test -run @ -bench BenchmarkParser -memprofile mem.out
	go tool pprof --lines --web --alloc_space *.test mem.out
