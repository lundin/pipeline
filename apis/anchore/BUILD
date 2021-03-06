subinclude("///pleasings2//openapi")

anchore_version = "v0.8.0"

genrule(
    name = "spec",
    srcs = [remote_file(
        name = "spec",
        _tag = "download",
        url = f"https://raw.githubusercontent.com/anchore/anchore-engine/{anchore_version}/anchore_engine/services/apiext/swagger/swagger.yaml",
        out = "swagger.yml", # important to avoid file name collision
        hashes = ["68a026c241503c521b9aa15aba94e1915ebcd755c1e44692c917b2f7b6c10d45"],
        licences = ["Apache-2.0"],
    )],
    outs = ["apis/anchore/swagger.yaml"],
    cmd = "cat \"$SRCS\" | tr '\\n' '\\r' | sed $'s/- Images\\r      - Vulnerabilities/- Images/g' | tr '\\r' '\\n' | sed '/- Image Content/d; /- Policy Evaluation/d; /- Queries/d' > \"$OUTS\"",
)

genrule(
    name = "anchore",
    srcs = [openapi_library(
        name = "_anchore#openapi",
        spec = ":spec",
        generator = "go",
        out = "tmp/.gen/anchore",
        additional_properties = {
            "packageName": "anchore",
            "withGoCodegenComment": "true",
            "sourceFolder": "anchore",
        },
        strip = ["{.travis.yml,git_push.sh,go.*,docs}"],
    )],
    outs = [".gen/anchore"],
    cmd = " && ".join([
        "sed 's/whitelist_ids,omitempty/whitelist_ids/' \"$SRCS/model_mapping_rule.go\" > \"$SRCS/model_mapping_rule.go.new\"",
        'mv "$SRCS/model_mapping_rule.go.new" "$SRCS/model_mapping_rule.go"',
        "sed 's/params,omitempty/params/' \"$SRCS/model_policy_rule.go\" > \"$SRCS/model_policy_rule.go.new\"",
        'mv "$SRCS/model_policy_rule.go.new" "$SRCS/model_policy_rule.go"',
        'mv "$SRCS" "$OUTS"',
    ]),
)

openapi_validate(
    name = "validate",
    spec = ":spec",
)
