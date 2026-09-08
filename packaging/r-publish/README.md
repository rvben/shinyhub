# ShinyHub RStudio addins

Install the ShinyHub CLI (`uv tool install shinyhub`), then install this local R
package with `R CMD INSTALL packaging/r-publish`. It depends on `rstudioapi` and
`jsonlite`. The addins currently support RStudio on macOS and Linux; Windows
users can use the CLI directly.

Choose **Connect to ShinyHub** in the Addins menu and sign in in your browser.
Then open your app and choose **Publish app to ShinyHub**. Select the destination,
review Doctor and Plan in the Console, and choose **Deploy** to follow deployment
in the Terminal. The CLI applies the exact saved bundle you reviewed and prints
the app URL; later source edits cannot change it. Temporary plan files are
removed after apply or cancellation. Local development and the preflight steps have
addins. For fleet checkouts, an active file selects the nearest app directory.

The addins use the CLI credential store and never read tokens. To select an
explicit CLI installation, set `options(shinyhub.executable = '/path/to/shinyhub')`.
Long-running development and deployment commands run in the Terminal, leaving
the R session available. Readiness checks run synchronously in the Console.
