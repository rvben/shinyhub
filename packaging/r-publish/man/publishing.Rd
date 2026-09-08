\name{publishing}
\alias{check_app}
\alias{preview_deploy}
\alias{publish_app}
\alias{develop_app}
\alias{connect_server}
\title{Develop and Publish ShinyHub Applications from RStudio}
\description{RStudio addins that call the installed ShinyHub CLI on macOS and Linux.}
\usage{
check_app()
preview_deploy()
publish_app()
develop_app()
connect_server()
}
\details{
\code{connect_server} signs in through the CLI browser flow.
\code{check_app} runs deployment diagnostics. \code{preview_deploy} shows a
read-only deployment plan. \code{publish_app} saves open documents, checks the
application, and shows an exact saved plan in the Console. After confirmation,
it applies that plan in the Terminal and prints the app URL.
\code{develop_app} starts local development with healthy reloads.

The active file selects the nearest app within the RStudio project. Server
selection uses the existing CLI credential store; tokens are never read by the
addin. Set \code{options(shinyhub.executable = "/path/to/shinyhub")} to use an
explicit CLI executable. Requires an active RStudio session.
}
\value{A terminal identifier, invisibly, for terminal commands; otherwise invisible \code{NULL}.}
