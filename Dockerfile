FROM python:3.11-slim

# Set working directory inside the container
WORKDIR /app

# Copy requirements and install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the exporter script
COPY sb8200_exporter.py .

# Define environment variables for Flask
ENV FLASK_APP=sb8200_exporter.py
ENV FLASK_RUN_HOST=0.0.0.0

# Expose the port used by the exporter
EXPOSE 9800

# Run the exporter
ENTRYPOINT ["python", "sb8200_exporter.py"]