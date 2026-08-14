# Sidecar de ML (Fase 5/6)
FROM python:3.12-slim
WORKDIR /ml
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
# Los modelos entrenados se montan como volumen (./ml/models)
VOLUME ["/ml/models"]
EXPOSE 8099
CMD ["uvicorn", "api:app", "--host", "0.0.0.0", "--port", "8099"]
