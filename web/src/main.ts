import "./style.css";
import { crowbanApp } from "./app";
import Alpine from "alpinejs";

Alpine.data("app", crowbanApp);
void Alpine.start();
