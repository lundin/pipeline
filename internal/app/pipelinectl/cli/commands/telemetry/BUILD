subinclude("///pleasings2//go:compat")

go_library(
    name = "telemetry",
    srcs = glob(
        ["*.go"],
        exclude = ["*_test.go"],
    ),
    visibility = ["PUBLIC"],
    deps = [],
)

go_test(
    name = "test",
    srcs = glob(["*_test.go"]),
    deps = [":telemetry"],
)
