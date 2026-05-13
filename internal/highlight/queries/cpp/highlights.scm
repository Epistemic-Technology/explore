; C++ highlights.

(comment) @comment

(string_literal) @string
(raw_string_literal) @string
(char_literal) @string
(escape_sequence) @string.escape

(number_literal) @number

(true) @constant.builtin
(false) @constant.builtin
(null) @constant.builtin
"nullptr" @constant.builtin
(this) @variable.builtin
(virtual) @keyword

[
  "alignas"
  "alignof"
  "break"
  "case"
  "catch"
  "class"
  "co_await"
  "co_return"
  "co_yield"
  "concept"
  "const"
  "constexpr"
  "constinit"
  "consteval"
  "continue"
  "default"
  "delete"
  "do"
  "else"
  "enum"
  "explicit"
  "extern"
  "final"
  "for"
  "friend"
  "if"
  "inline"
  "mutable"
  "namespace"
  "new"
  "noexcept"
  "operator"
  "override"
  "private"
  "protected"
  "public"
  "register"
  "requires"
  "return"
  "sizeof"
  "static"
  "static_assert"
  "struct"
  "switch"
  "template"
  "thread_local"
  "throw"
  "try"
  "typedef"
  "typename"
  "union"
  "using"
  "volatile"
  "while"
] @keyword

(primitive_type) @type.builtin
(sized_type_specifier) @type.builtin
(type_identifier) @type
(namespace_identifier) @type

(function_declarator declarator: (identifier) @function)
(function_declarator declarator: (field_identifier) @function)
(function_declarator declarator: (qualified_identifier name: (identifier) @function))
(function_declarator declarator: (qualified_identifier name: (field_identifier) @function))
(destructor_name (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
(call_expression function: (qualified_identifier name: (identifier) @function.call))

(field_expression field: (field_identifier) @property)

(preproc_include) @attribute
(preproc_def) @attribute
(preproc_function_def) @attribute
(preproc_call) @attribute
