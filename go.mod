module github.com/example/openai-cost-optimal-gateway

go 1.24.3

require (
    github.com/expr-lang/expr v0.0.0
    github.com/pkoukk/tiktoken-go v0.0.0
    github.com/tidwall/gjson v0.0.0
    github.com/tidwall/sjson v0.0.0
)

replace github.com/expr-lang/expr => ./github.com/expr-lang/expr
replace github.com/pkoukk/tiktoken-go => ./github.com/pkoukk/tiktoken-go
replace github.com/tidwall/gjson => ./github.com/tidwall/gjson
replace github.com/tidwall/sjson => ./github.com/tidwall/sjson

