import streamlit as st

st.set_page_config(page_title="Streamlit · ShinyHub Demo", page_icon="✦", layout="centered")

st.caption("Live application · Streamlit")
st.title("Turn an input into an answer.")
st.write(
    "Move the slider and Streamlit reruns this application instantly. "
    "It uses the same ShinyHub deployment and lifecycle controls as every other framework in the gallery."
)

count = st.slider("Input value", 0, 100, 25, help="Choose any whole number from 0 to 100.")
st.metric("Squared result", f"{count**2:,}")
