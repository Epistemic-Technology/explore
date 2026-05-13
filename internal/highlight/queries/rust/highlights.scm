; Rust highlights.

(line_comment) @comment
(block_comment) @comment

(string_literal) @string
(raw_string_literal) @string
(char_literal) @string
(escape_sequence) @string.escape

(integer_literal) @number
(float_literal) @number

(boolean_literal) @constant.builtin

[
  "as"
  "async"
  "await"
  "break"
  "const"
  "continue"
  "dyn"
  "else"
  "enum"
  "extern"
  "fn"
  "for"
  "if"
  "impl"
  "in"
  "let"
  "loop"
  "match"
  "mod"
  "move"
  "pub"
  "ref"
  "return"
  "static"
  "struct"
  "trait"
  "type"
  "union"
  "unsafe"
  "use"
  "where"
  "while"
] @keyword

(self) @variable.builtin

(function_item name: (identifier) @function)
(function_signature_item name: (identifier) @function)
(struct_item name: (type_identifier) @type)
(enum_item name: (type_identifier) @type)
(trait_item name: (type_identifier) @type)
(type_item name: (type_identifier) @type)
(union_item name: (type_identifier) @type)
(mod_item name: (identifier) @type)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
(call_expression function: (scoped_identifier name: (identifier) @function.call))

(type_identifier) @type
(primitive_type) @type.builtin

(field_identifier) @property

(const_item name: (identifier) @constant)
(attribute_item) @attribute
(inner_attribute_item) @attribute
