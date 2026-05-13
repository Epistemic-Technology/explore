; Python highlights.

(comment) @comment

(string) @string
(escape_sequence) @string.escape

(integer) @number
(float) @number

(true) @constant.builtin
(false) @constant.builtin
(none) @constant.builtin

[
  "and"
  "as"
  "assert"
  "async"
  "await"
  "break"
  "class"
  "continue"
  "def"
  "del"
  "elif"
  "else"
  "except"
  "finally"
  "for"
  "from"
  "global"
  "if"
  "import"
  "in"
  "is"
  "lambda"
  "nonlocal"
  "not"
  "or"
  "pass"
  "raise"
  "return"
  "try"
  "while"
  "with"
  "yield"
] @keyword

(function_definition name: (identifier) @function)
(class_definition name: (identifier) @type)

(call function: (identifier) @function.call)
(call function: (attribute attribute: (identifier) @function.call))

(decorator) @attribute

(attribute attribute: (identifier) @property)

; Builtins
((identifier) @variable.builtin
 (#match? @variable.builtin "^(self|cls)$"))
((identifier) @function.call
 (#match? @function.call "^(print|len|range|enumerate|map|filter|zip|sorted|reversed|isinstance|hasattr|getattr|setattr|str|int|float|bool|list|dict|set|tuple|type|repr|open|input|abs|min|max|sum|any|all)$"))
