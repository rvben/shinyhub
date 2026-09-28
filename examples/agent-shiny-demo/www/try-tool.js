document.addEventListener("DOMContentLoaded", function () {
  var button = document.getElementById("try-agent-tool");
  var result = document.getElementById("agent-tool-result");
  button.addEventListener("click", async function () {
    button.disabled = true;
    result.textContent = "Waiting for the Shiny session…";
    try {
      var applied = await window.shinyhubAgentTools.invoke("set_dashboard_period", {
        period: "This year"
      });
      result.textContent = "Applied by Shiny: " + applied.period + " · " + applied.requests + " requests";
    } catch (error) {
      result.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
});
