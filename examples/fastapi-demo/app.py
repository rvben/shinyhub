from fastapi import FastAPI

app = FastAPI(title="ShinyHub API example")


@app.get("/")
def index():
    return {"service": "ShinyHub API example", "status": "ready"}


@app.get("/add")
def add(a: float = 1, b: float = 2):
    return {"result": a + b}
