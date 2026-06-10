import streamlit as st

st.title("ShinyHub Streamlit demo")
st.write("Deployed with a manifest [app] command - no custom image needed.")
count = st.slider("Pick a number", 0, 100, 25)
st.metric("Squared", count**2)
