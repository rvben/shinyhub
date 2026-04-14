library(shiny)

ui <- fluidPage(titlePanel("Hello from R!"))
server <- function(input, output) {}
shinyApp(ui = ui, server = server)
