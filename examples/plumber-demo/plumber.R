#* Service status
#* @get /
#* @serializer unboxedJSON
function() {
  list(service = "ShinyHub Plumber example", status = "ready")
}

#* Add two numbers
#* @param a First number
#* @param b Second number
#* @get /add
#* @serializer unboxedJSON
function(a = 1, b = 2) {
  list(result = as.numeric(a) + as.numeric(b))
}
