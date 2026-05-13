; Go highlights — subset matching nvim-treesitter naming conventions.

; Comments
(comment) @comment

; Strings & runes
(interpreted_string_literal) @string
(raw_string_literal) @string
(rune_literal) @string

; Numbers
(int_literal) @number
(float_literal) @number
(imaginary_literal) @number

; Booleans / nil
(true) @constant.builtin
(false) @constant.builtin
(nil) @constant.builtin

; Keywords
[
  "break"
  "case"
  "chan"
  "const"
  "continue"
  "default"
  "defer"
  "else"
  "fallthrough"
  "for"
  "func"
  "go"
  "goto"
  "if"
  "import"
  "interface"
  "map"
  "package"
  "range"
  "return"
  "select"
  "struct"
  "switch"
  "type"
  "var"
] @keyword

; Function decls
(function_declaration name: (identifier) @function)
(method_declaration name: (field_identifier) @function.method)

; Function calls
(call_expression function: (identifier) @function.call)
(call_expression function: (selector_expression field: (field_identifier) @function.call))

; Types
(type_identifier) @type
(type_spec name: (type_identifier) @type)
((type_identifier) @type.builtin
 (#match? @type.builtin "^(any|bool|byte|complex(64|128)|error|float(32|64)|int(8|16|32|64)?|rune|string|uint(8|16|32|64)?|uintptr)$"))

; Properties / fields
(field_identifier) @property

; Package name in import paths is already covered by @string above.
