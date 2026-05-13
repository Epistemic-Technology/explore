; TypeScript highlights (also used for TSX).

(comment) @comment

(string) @string
(template_string) @string
(string_fragment) @string
(escape_sequence) @string.escape

(number) @number

(true) @constant.builtin
(false) @constant.builtin
(null) @constant.builtin
(undefined) @constant.builtin

[
  "as"
  "async"
  "await"
  "break"
  "case"
  "catch"
  "class"
  "const"
  "continue"
  "debugger"
  "default"
  "delete"
  "do"
  "else"
  "enum"
  "export"
  "extends"
  "finally"
  "for"
  "from"
  "function"
  "get"
  "if"
  "implements"
  "import"
  "in"
  "instanceof"
  "interface"
  "let"
  "namespace"
  "new"
  "of"
  "private"
  "protected"
  "public"
  "readonly"
  "return"
  "satisfies"
  "set"
  "static"
  "switch"
  "throw"
  "try"
  "type"
  "typeof"
  "var"
  "void"
  "while"
  "with"
  "yield"
] @keyword

(function_declaration name: (identifier) @function)
(method_definition name: (property_identifier) @function.method)
(method_signature name: (property_identifier) @function.method)
(class_declaration name: (type_identifier) @type)
(interface_declaration name: (type_identifier) @type)
(type_alias_declaration name: (type_identifier) @type)
(enum_declaration name: (identifier) @type)

(call_expression function: (identifier) @function.call)
(call_expression function: (member_expression property: (property_identifier) @function.call))

(type_identifier) @type
(predefined_type) @type.builtin

(property_identifier) @property
