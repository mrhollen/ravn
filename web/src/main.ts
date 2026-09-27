import "./style.css";
import { crowbanApp } from "./app";
import Alpine from "alpinejs";

(window as unknown as { Alpine: typeof Alpine }).Alpine = Alpine;
Alpine.data("app", crowbanApp);
void Alpine.start();
