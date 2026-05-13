; Java highlights.

(line_comment) @comment
(block_comment) @comment

(string_literal) @string
(character_literal) @string
(escape_sequence) @string.escape

(decimal_integer_literal) @number
(hex_integer_literal) @number
(octal_integer_literal) @number
(binary_integer_literal) @number
(decimal_floating_point_literal) @number
(hex_floating_point_literal) @number

(true) @constant.builtin
(false) @constant.builtin
(null_literal) @constant.builtin
(this) @variable.builtin

[
  "abstract"
  "assert"
  "break"
  "case"
  "catch"
  "class"
  "continue"
  "default"
  "do"
  "else"
  "enum"
  "extends"
  "final"
  "finally"
  "for"
  "if"
  "implements"
  "import"
  "instanceof"
  "interface"
  "native"
  "new"
  "package"
  "private"
  "protected"
  "public"
  "record"
  "return"
  "static"
  "strictfp"
  "switch"
  "synchronized"
  "throw"
  "throws"
  "transient"
  "try"
  "volatile"
  "while"
  "yield"
] @keyword

(void_type) @type.builtin
(integral_type) @type.builtin
(floating_point_type) @type.builtin
(boolean_type) @type.builtin
(generic_type (type_identifier) @type)
(type_identifier) @type

(method_declaration name: (identifier) @function)
(constructor_declaration name: (identifier) @function)
(method_invocation name: (identifier) @function.call)

(annotation name: (identifier) @attribute)
(marker_annotation name: (identifier) @attribute)

(field_declaration declarator: (variable_declarator name: (identifier) @property))
