library(shiny)

ui <- fluidPage(
  tags$head(tags$style(HTML("
    :root {
      color-scheme:dark;
      --canvas:#060914;
      --surface:#0e1426;
      --surface-raised:#141b32;
      --surface-hover:#1b2444;
      --line:#2b3a63;
      --text:#e8eeff;
      --text-soft:#a8b4d4;
      --accent:#13b8a6;
      --accent-bright:#5eead4;
    }
    body { margin:0; background:var(--canvas); color:var(--text); font-family:Manrope,-apple-system,system-ui,\"Segoe UI\",sans-serif; }
    .container-fluid { max-width:1100px; padding:32px 20px 60px; }
    h1 { max-width:22ch; margin-bottom:8px; font-size:3rem; line-height:1.08; letter-spacing:-.03em; text-wrap:balance; }
    .lede { max-width:65ch; margin-bottom:28px; color:var(--text-soft); font-size:17px; line-height:1.55; text-wrap:pretty; }
    .well, .card { background:var(--surface); color:var(--text); border:1px solid var(--line); border-radius:14px; box-shadow:none; }
    .well { padding:18px; }
    .card { padding:18px; margin-bottom:14px; }
    .metric { color:var(--accent-bright); font-size:32px; font-weight:750; letter-spacing:-.03em; }
    .label { color:var(--text-soft); letter-spacing:.055em; text-transform:uppercase; font-size:11px; font-weight:650; }
    table { color:var(--text); }
    .table>tbody>tr>td, .table>thead>tr>th { border-color:var(--line); }
    .form-control, .selectize-input, .selectize-dropdown, .selectize-dropdown-content {
      min-height:44px; border-color:var(--line) !important; background:var(--surface-raised) !important; color:var(--text) !important;
    }
    .selectize-dropdown .active { background:var(--surface-hover) !important; color:var(--text) !important; }
    .checkbox label { min-height:44px; display:flex; align-items:center; gap:8px; color:var(--text); }
    input:focus-visible, .selectize-input.focus { outline:3px solid color-mix(in srgb, var(--accent) 42%, transparent); outline-offset:2px; }
    .irs--shiny .irs-line { background:var(--surface-hover); border-color:var(--line); }
    .irs--shiny .irs-bar, .irs--shiny .irs-single { background:var(--accent); border-color:var(--accent); }
    .irs--shiny .irs-handle { border-color:var(--accent); background:var(--text); }
    .irs--shiny .irs-min, .irs--shiny .irs-max, .irs--shiny .irs-grid-text { color:var(--text-soft); }
    @media (max-width:767px) { .container-fluid { padding:24px 14px 40px; } h1 { font-size:2.25rem; } }
  "))),
  div(class = "label", "SHINYHUB · R SHINY"),
  h1("Explore the shape of performance."),
  p(class = "lede", "Filter the built-in mtcars dataset and compare efficiency across transmission types. No static mockup: every control below is reactive R Shiny."),
  fluidRow(
    column(3, wellPanel(
      sliderInput("mpg", "Miles per gallon", min(mtcars$mpg), max(mtcars$mpg), range(mtcars$mpg)),
      checkboxGroupInput("am", "Transmission", choices = c("Automatic" = 0, "Manual" = 1), selected = c(0, 1)),
      selectInput("cyl", "Cylinders", choices = c("All", 4, 6, 8))
    )),
    column(9,
      fluidRow(
        column(4, div(class="card", div(class="label", "Matching cars"), div(class="metric", textOutput("count", inline=TRUE)))),
        column(4, div(class="card", div(class="label", "Average MPG"), div(class="metric", textOutput("avg", inline=TRUE)))),
        column(4, div(class="card", div(class="label", "Best MPG"), div(class="metric", textOutput("best", inline=TRUE))))
      ),
      div(class="card", tableOutput("cars"))
    )
  )
)

server <- function(input, output, session) {
  filtered <- reactive({
    cars <- mtcars
    cars$model <- rownames(cars)
    cars <- cars[cars$mpg >= input$mpg[1] & cars$mpg <= input$mpg[2], ]
    cars <- cars[cars$am %in% as.numeric(input$am), ]
    if (input$cyl != "All") cars <- cars[cars$cyl == as.numeric(input$cyl), ]
    cars
  })
  output$count <- renderText(nrow(filtered()))
  output$avg <- renderText(if (nrow(filtered())) sprintf("%.1f", mean(filtered()$mpg)) else "—")
  output$best <- renderText(if (nrow(filtered())) sprintf("%.1f", max(filtered()$mpg)) else "—")
  output$cars <- renderTable({
    x <- filtered()[, c("model", "mpg", "cyl", "hp", "wt")]
    names(x) <- c("Model", "MPG", "Cyl", "HP", "Weight")
    head(x[order(-x$MPG), ], 10)
  }, rownames = FALSE)
}

shinyApp(ui, server)
