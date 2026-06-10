# streamlit-demo

A minimal Streamlit app that shows how to deploy a non-Shiny Python framework
to ShinyHub using a manifest `[app] command`. The `shinyhub.toml` supplies the
full launch command with `{port}` and `{host}` placeholders; no custom Docker
image is needed.

See [`docs/recipes/streamlit.md`](../../docs/recipes/streamlit.md) for the
full walkthrough including CORS/XSRF notes and persistent-data usage.

## Deploy

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/streamlit-demo --slug streamlit-demo
```

After deploy, the app is live at `https://your-shinyhub.example.com/app/streamlit-demo/`.
