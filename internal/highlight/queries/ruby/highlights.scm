; Ruby highlights.

(comment) @comment

(string) @string
(heredoc_body) @string
(heredoc_beginning) @string
(escape_sequence) @string.escape

(simple_symbol) @string.special
(hash_key_symbol) @string.special
(delimited_symbol) @string.special

(integer) @number
(float) @number

(true) @constant.builtin
(false) @constant.builtin
(nil) @constant.builtin
(self) @variable.builtin

[
  "alias"
  "and"
  "begin"
  "break"
  "case"
  "class"
  "def"
  "do"
  "else"
  "elsif"
  "end"
  "ensure"
  "for"
  "if"
  "in"
  "module"
  "next"
  "not"
  "or"
  "rescue"
  "return"
  "then"
  "unless"
  "until"
  "when"
  "while"
  "yield"
] @keyword

(method name: (identifier) @function)
(method name: (constant) @function)
(singleton_method name: (identifier) @function)
(singleton_method name: (constant) @function)

(class name: (constant) @type)
(class name: (scope_resolution) @type)
(module name: (constant) @type)
(module name: (scope_resolution) @type)

(call method: (identifier) @function.call)
(call method: (constant) @function.call)

(assignment left: (constant) @constant)
(instance_variable) @variable.instance
(class_variable) @variable.instance
(global_variable) @variable.builtin
